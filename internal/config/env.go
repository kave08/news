package config

import "os"

func syscallEnv(key string) (string, bool) {
	return os.LookupEnv(key)
}
