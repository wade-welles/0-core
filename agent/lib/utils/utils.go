package utils

import (
	"fmt"
	"github.com/naoina/toml"
	"io/ioutil"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var validLevels = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 20, 21, 22, 23, 30}

//Expand expands a string of the format 1,2,3-5 or * to the valid list of log levels
func Expand(s string) ([]int, error) {
	levels := make(map[int]bool)

	for _, part := range strings.Split(s, ",") {
		part := strings.Trim(part, " ")

		boundries := strings.Split(part, "-")
		lower := strings.Trim(boundries[0], " ")
		lowerValue := 0
		upperValue := 0
		hasUpper := false

		if len(boundries) > 1 {

			upperValue64, err := strconv.ParseInt(strings.Trim(boundries[1], " "), 10, 32)
			if err != nil {
				return nil, err
			}

			hasUpper = true
			upperValue = int(upperValue64)
		}

		if lower == "*" {
			for _, l := range validLevels {
				levels[l] = true
			}
			continue
		}

		lowerValue64, err := strconv.ParseInt(lower, 10, 32)
		if err != nil {
			return nil, err
		}

		lowerValue = int(lowerValue64)

		if !hasUpper {
			if In(validLevels, lowerValue) {
				levels[lowerValue] = true
			}

			continue
		}

		if upperValue > 30 {
			upperValue = 30
		}

		for i := lowerValue; i <= upperValue; i++ {
			if In(validLevels, i) {
				levels[i] = true
			}
		}
	}

	result := make([]int, 0, len(levels))
	for key := range levels {
		result = append(result, key)
	}

	sort.Ints(result)
	return result, nil
}

var formatPattern = regexp.MustCompile("{[^}]+}")

//Format accepts a string pattern of format "something {key}, something" and replaces
//all occurences of {<key>} from the values map.
func Format(pattern string, values map[string]interface{}) string {
	return formatPattern.ReplaceAllStringFunc(pattern, func(m string) string {
		key := strings.TrimRight(strings.TrimLeft(m, "{"), "}")
		return fmt.Sprintf("%v", values[key])
	})
}

//In checks if x is in l
func In(l []int, x int) bool {
	for i := 0; i < len(l); i++ {
		if l[i] == x {
			return true
		}
	}

	return false
}

//GetKeys returns the keys of map
func GetKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	return keys
}

//InString checks if x is in l
func InString(l []string, x string) bool {
	for i := 0; i < len(l); i++ {
		if l[i] == x {
			return true
		}
	}

	return false
}

//Update updates dst map from value in src
func Update(dst map[string]interface{}, src map[string]interface{}) {
	for k, v := range src {
		dst[k] = v
	}
}

//LoadTomlFile loads toml using "github.com/naoina/toml"
func LoadTomlFile(filename string, v interface{}) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	buf, err := ioutil.ReadAll(f)
	if err != nil {
		return err
	}
	if err := toml.Unmarshal(buf, v); err != nil {
		return err
	}

	return nil
}

func Exists(name string) bool {
	_, err := os.Stat(name)
	if err == nil {
		return true
	}

	return !os.IsNotExist(err)
}
