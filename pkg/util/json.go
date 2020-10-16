package util

import "encoding/json"

func JsonWrapper(obj interface{}) string {
	if jsonStr, jsonErr := json.Marshal(obj); jsonErr == nil {
		return string(jsonStr)
	}
	return "json_format_error"
}
