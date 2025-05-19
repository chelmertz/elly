package points

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/chelmertz/elly/internal/types"
)

type GoldenTest struct {
	PrDomain    types.ViewPr
	Points      Points
	CurrentUser string    // the points are calculated for this user, persist this
	CurrentTime time.Time // older or newer PRs might be calculated differently
}

// Exits at failure, should only affect manual debugging sessions. Normal usage
// of elly should not include the -golden flag.
func StoreGoldenTest(data GoldenTest) {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Fatalf("Failed to marshal golden test data: %v", err)
	}

	filePath := testFilePath(data.PrDomain.Id())
	writeErr := os.WriteFile(filePath, jsonData, 0644)
	if writeErr != nil {
		log.Fatalf("Failed to write golden test data to %s: %v", filePath, writeErr)
	}
}

// Assume we're running from the root of the repo,
func testFilePath(prId string) string {
	return filepath.Join("internal", "points", fmt.Sprintf("golden_test_%s.json", prId))
}
