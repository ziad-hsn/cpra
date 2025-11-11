package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run yaml_to_json.go <yaml_file>")
		os.Exit(1)
	}

	yamlFile := os.Args[1]
	jsonFile := strings.TrimSuffix(yamlFile, ".yaml") + ".json"

	fmt.Printf("Converting %s to %s...\n", yamlFile, jsonFile)

	// Read YAML file
	yamlData, err := os.ReadFile(yamlFile)
	if err != nil {
		fmt.Printf("Error reading YAML file: %v\n", err)
		os.Exit(1)
	}

	// Parse YAML
	var data interface{}
	err = yaml.Unmarshal(yamlData, &data)
	if err != nil {
		fmt.Printf("Error parsing YAML: %v\n", err)
		os.Exit(1)
	}

	// Convert to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		fmt.Printf("Error converting to JSON: %v\n", err)
		os.Exit(1)
	}

	// Write JSON file
	err = os.WriteFile(jsonFile, jsonData, 0644)
	if err != nil {
		fmt.Printf("Error writing JSON file: %v\n", err)
		os.Exit(1)
	}

	// Get file sizes
	yamlInfo, _ := os.Stat(yamlFile)
	jsonInfo, _ := os.Stat(jsonFile)

	fmt.Printf("Conversion complete!\n")
	fmt.Printf("YAML size: %d bytes (%.1f MB)\n", yamlInfo.Size(), float64(yamlInfo.Size())/1024/1024)
	fmt.Printf("JSON size: %d bytes (%.1f MB)\n", jsonInfo.Size(), float64(jsonInfo.Size())/1024/1024)
	fmt.Printf("Size ratio: %.2fx\n", float64(jsonInfo.Size())/float64(yamlInfo.Size()))
}
