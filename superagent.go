package main

import (
    "github.com/Jumpscale/jsagent/agent"
    "github.com/Jumpscale/jsagent/agent/lib/pm"
    "github.com/Jumpscale/jsagent/agent/lib/logger"
    "github.com/Jumpscale/jsagent/agent/lib/stats"
    "github.com/Jumpscale/jsagent/agent/lib/utils"
    "github.com/Jumpscale/jsagent/agent/lib/builtin"
    "github.com/shirou/gopsutil/process"
    "time"
    "encoding/json"
    "net/http"
    "log"
    "fmt"
    "strings"
    "bytes"
    "flag"
    "os"
    "io/ioutil"
    "crypto/tls"
    "crypto/x509"
)

const (
    CMD_GET_MSGS = "get_msgs"
)


type Controller struct {
    URL string
    Client *http.Client
}

func getHttpClient(security *agent.Security) *http.Client {
    var tlsConfig tls.Config

    if security.CertificateAuthority != "" {
        pem, err := ioutil.ReadFile(security.CertificateAuthority)
        if err != nil {
            log.Fatal(err)
        }

        tlsConfig.RootCAs = x509.NewCertPool()
        tlsConfig.RootCAs.AppendCertsFromPEM(pem)
    }

    if security.ClientCertificate != "" {
        if security.ClientCertificateKey == "" {
            log.Fatal("Missing certificate key file")
        }
        // pem, err := ioutil.ReadFile(security.ClientCertificate)
        // if err != nil {
        //     log.Fatal(err)
        // }

        cert, err := tls.LoadX509KeyPair(security.ClientCertificate,
            security.ClientCertificateKey)
        if err != nil {
            log.Fatal(err)
        }

        tlsConfig.Certificates = []tls.Certificate{cert}
    }

    return &http.Client{
        Timeout: 60 * time.Second,
        Transport: &http.Transport{
            DisableKeepAlives: true,
            Proxy: http.ProxyFromEnvironment,
            TLSHandshakeTimeout: 10 * time.Second,
            TLSClientConfig: &tlsConfig,
        },
    }
}
/*
This function will register a handler to the get_msgs function
This one is done here and NOT in the 'buildin' library because

1- we need to know where to find the db files, this will not be availble until
   the time we are registering the DB logger. If the db logger is not configured
   in the first place, then the get_msgs will not be possible.
2- Moving this register function to the build-in will cause cyclic dependencies.
*/

func registGetMsgsFunction(path string) {
    querier := logger.NewDBMsgQuery(path)

    get_msgs := func(cmd *pm.Cmd, cfg pm.RunCfg) *pm.JobResult {
        result := pm.NewBasicJobResult(cmd)
        result.StartTime = int64(time.Duration(time.Now().UnixNano()) / time.Millisecond)

        defer func() {
            endtime := time.Duration(time.Now().UnixNano()) / time.Millisecond
            result.Time = int64(endtime) - result.StartTime
        }()

        query := logger.Query{}

        err := json.Unmarshal([]byte(cmd.Data), &query)
        if err != nil {
            log.Println("Failed to parse get_msgs query", err)
        }

        //we still can continue the query even if we have unmarshal errors.

        result_chn, err := querier.Query(query)

        if err != nil {
            result.State = pm.S_ERROR
            result.Data = fmt.Sprintf("%v", err)

            return result
        }

        records := make([]logger.Result, 0, 1000)
        for record := range result_chn {
            records = append(records, record)
        }

        data, err := json.Marshal(records)
        if err != nil {
            result.State = pm.S_ERROR
            result.Data = fmt.Sprintf("%v", err)

            return result
        }

        result.State = pm.S_SUCCESS
        result.Data = string(data)

        return result
    }

    pm.CMD_MAP[CMD_GET_MSGS] = builtin.InternalProcessFactory(get_msgs)
}

func getKeys(m map[string]Controller) []string {
    keys := make([]string, 0,len(m))
    for key, _ := range m {
        keys = append(keys, key)
    }

    return keys
}

func main() {
    settings := agent.Settings{}
    var cfg string
    var help bool

    flag.BoolVar(&help, "h", false, "Print this help screen")
    flag.StringVar(&cfg, "c", "", "Path to config file")
    flag.Parse()

    printHelp := func() {
        fmt.Println("agent [options]")
        flag.PrintDefaults()
    }

    if help {
        printHelp()
        return
    }

    if cfg == "" {
        fmt.Println("Missing required option -c")
        flag.PrintDefaults()
        os.Exit(1)
    }

    utils.LoadTomlFile(cfg, &settings)

    //loading command history file
    //history file is used to remember long running jobs during reboots.
    var history []*pm.Cmd
    hisstr, err := ioutil.ReadFile(settings.Main.HistoryFile)

    if err == nil {
        err = json.Unmarshal(hisstr, &history)
        if err != nil {
            log.Println("Failed to load history file, invalid syntax ", err)
            history = make([]*pm.Cmd, 0)
        }
    } else {
        log.Println("Couldn't read history file")
        history = make([]*pm.Cmd, 0)
    }

    //dump hisory file
    dumpHistory := func() {
        data, err := json.Marshal(history)
        if err != nil {
            log.Fatal("Failed to write history file")
        }

        ioutil.WriteFile(settings.Main.HistoryFile, data, 0644)
    }

    buildUrl := func (base string, endpoint string) string {
        base = strings.TrimRight(base, "/")
        return fmt.Sprintf("%s/%d/%d/%s", base,
            settings.Main.Gid,
            settings.Main.Nid,
            endpoint)
    }

    //build list with ACs that we will poll from.
    controllers := make(map[string]Controller)
    for key, controllerCfg := range settings.Controllers {
        controllers[key] = Controller{
            URL: controllerCfg.URL,
            Client: getHttpClient(&controllerCfg.Security),
        }
    }

    mgr := pm.NewPM(settings.Main.MessageIdFile, settings.Main.MaxJobs)

    //apply logging handlers.
    dbLoggerConfigured := false
    for _, logcfg := range settings.Logging {
        switch strings.ToLower(logcfg.Type) {
            case "db":
                if dbLoggerConfigured {
                    log.Fatal("Only one db logger can be configured")
                }
                sqlFactory := logger.NewSqliteFactory(logcfg.LogDir)
                handler := logger.NewDBLogger(sqlFactory, logcfg.Levels)
                mgr.AddMessageHandler(handler.Log)
                registGetMsgsFunction(logcfg.LogDir)

                dbLoggerConfigured = true
            case "ac":
                endpoints := make(map[string]*http.Client)

                if len(logcfg.Controllers) > 0 {
                    //specific ones.
                    for _, key := range logcfg.Controllers {
                        controller, ok := controllers[key]
                        if !ok {
                            log.Panicf("Unknow controller '%s'", key)
                        }
                        url := buildUrl(controller.URL, "log")
                        endpoints[url] = controller.Client
                    }
                } else {
                    //all ACs
                    for _, controller := range controllers {
                        url := buildUrl(controller.URL, "log")
                        endpoints[url] = controller.Client
                    }
                }

                batchsize := 1000 // default
                flushint := 120 // default (in seconds)
                if logcfg.BatchSize != 0 {
                    batchsize = logcfg.BatchSize
                }
                if logcfg.FlushInt != 0 {
                    flushint = logcfg.FlushInt
                }

                handler := logger.NewACLogger(
                    endpoints,
                    batchsize,
                    time.Duration(flushint) * time.Second,
                    logcfg.Levels)
                mgr.AddMessageHandler(handler.Log)
            case "console":
                handler := logger.NewConsoleLogger(logcfg.Levels)
                mgr.AddMessageHandler(handler.Log)
            default:
                log.Panicf("Unsupported logger type: %s", logcfg.Type)
        }
    }

    mgr.AddStatsdMeterHandler(func (statsd *stats.Statsd, cmd *pm.Cmd, ps *process.Process) {
        //for each long running external process this will be called every 2 sec
        //You can here collect all the data you want abou the process and feed
        //statsd.

        cpu, err := ps.CPUPercent(0)
        if err == nil {
            statsd.Gauage("cpu", fmt.Sprintf("%f", cpu))
        }

        mem, err := ps.MemoryInfo()
        if err == nil {
            statsd.Gauage("rss", fmt.Sprintf("%d", mem.RSS))
            statsd.Gauage("vms", fmt.Sprintf("%d", mem.VMS))
            statsd.Gauage("swap", fmt.Sprintf("%d", mem.Swap))
        }
    })

    var statsKeys []string
    if len(settings.Stats.Controllers) > 0 {
        statsKeys = settings.Stats.Controllers
    } else {
        statsKeys = getKeys(controllers)
    }

    mgr.AddStatsFlushHandler(func (stats *stats.Stats) {
        //This will be called per process per stats_interval seconds. with
        //all the aggregated stats for that process.
        res, _ := json.Marshal(stats)
        for _, key := range statsKeys {
            controller, ok := controllers[key]
            if !ok {
                log.Printf("Stats: Unknow controller '%s'\n", key)
                continue
            }

            url := buildUrl(controller.URL, "stats")
            reader := bytes.NewBuffer(res)
            resp, err := controller.Client.Post(url, "application/json", reader)
            if err != nil {
                log.Println("Failed to send stats result to AC", url, err)
                return
            }
            resp.Body.Close()
        }
    })

    var pollKeys []string
    if len(settings.Channel.Cmds) > 0 {
        pollKeys = settings.Channel.Cmds
    } else {
        pollKeys = getKeys(controllers)
    }

    //start pollers goroutines
    for _, key := range pollKeys {
        go func() {
            lastfail := time.Now().Unix()
            controller, ok := controllers[key]
            if !ok {
                log.Panicf("Channel: Unknow controller '%s'", key)
            }

            client := controller.Client

            for {
                response, err := client.Get(buildUrl(controller.URL, "cmd"))
                if err != nil {
                    log.Println("No new commands, retrying ...", controller.URL, err)
                    //HTTP Timeout
                    if time.Now().Unix() - lastfail < 4 {
                        time.Sleep(4 * time.Second)
                    }
                    lastfail = time.Now().Unix()

                    continue
                }


                body, err := ioutil.ReadAll(response.Body)
                if err != nil {
                    log.Println("Failed to load response content", err)
                    continue
                }

                response.Body.Close()
                if response.StatusCode != 200 {
                    log.Println("Failed to retrieve jobs", response.Status, string(body))
                    time.Sleep(2 * time.Second)
                    continue
                }

                if len(body) == 0 {
                    //no data, can be a long poll timeout
                    continue
                }

                cmd, err := pm.LoadCmd(body)
                if err != nil {
                    log.Println("Failed to load cmd", err, string(body))
                    continue
                }

                //set command defaults
                //1 - stats_interval
                meterInt := cmd.Args.GetInt("stats_interval")
                if meterInt == 0 {
                    cmd.Args.Set("stats_interval", settings.Stats.Interval)
                }

                //tag command for routing.
                cmd.Args.SetTag(key)
                log.Println("Starting command", cmd)

                if cmd.Args.GetInt("max_time") == -1 {
                    //that's a long running process.
                    history = append(history, cmd)
                    dumpHistory()
                }

                mgr.RunCmd(cmd)
            }
        } ()
    }

    //handle process results
    mgr.AddResultHandler(func (result *pm.JobResult) {
        //send result to AC.
        res, _ := json.Marshal(result)
        controller := controllers[result.Args.GetTag()]

        url := buildUrl(controller.URL, "result")

        reader := bytes.NewBuffer(res)
        resp, err := controller.Client.Post(url, "application/json", reader)
        if err != nil {
            log.Println("Failed to send job result to AC", url, err)
            return
        }
        resp.Body.Close()
    })

    //register the execute commands
    for cmdKey, cmdCfg := range settings.Cmds {
        var env []string
        if len(cmdCfg.Env) > 0 {
            env = make([]string, 0, len(cmdCfg.Env))
            for ek, ev := range(cmdCfg.Env) {
                env = append(env, fmt.Sprintf("%v=%v", ek, ev))
            }
        }

        pm.RegisterCmd(cmdKey, cmdCfg.Binary, cmdCfg.Cwd, cmdCfg.Script, env)
    }

    //start process mgr.
    mgr.Run()

    //rerun history
    for i := 0; i < len(history); i ++ {
        cmd := history[i]
        meterInt := cmd.Args.GetInt("stats_interval")
        if meterInt == 0 {
            cmd.Args.Set("stats_interval", settings.Stats.Interval)
        }

        if err != nil {
            log.Println("Failed to load history command", history[i])
        }

        mgr.RunCmd(cmd)
    }

    event, _ := json.Marshal(map[string]string{
        "name": "startup",
    })


    // send startup event to all agent controllers
    for _, controller := range controllers {
        reader := bytes.NewBuffer(event)

        url := buildUrl(controller.URL, "event")

        resp, err := controller.Client.Post(url, "application/json", reader)
        if err != nil {
            log.Println("Failed to send startup event to AC", url, err)
            continue
        }
        resp.Body.Close()
    }

    //wait
    select {}
}
