package batt

import (
	"flag"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var Secret string

var secretFile = flag.String("secretfile", filepath.Join(homedir(), ".batt-secret"), "filename of secret")

func homedir() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("HOMEPATH")
	}
	return os.Getenv("HOME")
}

func Init() {
	flag.Parse()

	slurp, err := ioutil.ReadFile(*secretFile)
	if err != nil {
		log.Fatalf("Error reading secret file: %v", err)
	}
	Secret = strings.TrimSpace(string(slurp))
}
