package batt

import (
	"flag"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
)

var Secret string

var secretFile = flag.String("secretfile", filepath.Join(os.Getenv("HOME"), ".batt-secret"), "filename of secret")

func Init() {
	flag.Parse()

	slurp, err := ioutil.ReadFile(*secretFile)
	if err != nil {
		log.Fatalf("Error reading secret file: %v", err)
	}
	Secret = strings.TrimSpace(string(slurp))
}
