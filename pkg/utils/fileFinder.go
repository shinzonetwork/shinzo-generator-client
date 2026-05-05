package utils

import (
	"fmt"
	"os"
)

func FindFile(expectedPath string) (string, error) {
	possiblePaths := []string{
		expectedPath,                          // From project root
		fmt.Sprintf("../%s", expectedPath),    // From bin/ directory
		fmt.Sprintf("../../%s", expectedPath), // From pkg/*/ directory - test context
	}

	var filePath string
	var err error
	for _, path := range possiblePaths {
		if _, err = os.Stat(path); err == nil {
			filePath = path
			return filePath, nil
		}
	}

	return "", fmt.Errorf("Could not find file in any path searched: %v", possiblePaths)
}