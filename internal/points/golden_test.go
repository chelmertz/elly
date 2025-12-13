/*
server writes golden test as a file to this folder (after having checked that we're in the correct folder - i.e. look for a .git folder etc. and assume that we're good to go)
the test file contains a persisted json response (from github, if -golden and 200), a timestamp, the total points, and a marshalled string of the point awards (i.e. with reasoning)
a single test that loops over the saved golden tests and checks them against the current points.StandardPrPoints(), showing diff of points + reasoning on failure
*/
package points

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestGoldenTests(t *testing.T) {
	goldenTests := []GoldenTest{}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not get current file info")
	}

	thisDir := filepath.Dir(thisFile)
	err := filepath.Walk(thisDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			t.Fatalf("path error in Walk() for %s: %v", path, err)
		}

		if strings.HasPrefix(info.Name(), "golden_test_") && strings.HasSuffix(path, ".json") {
			goldenTests = append(goldenTests, loadGoldenTest(path))
		}

		return nil
	})
	if err != nil {
		t.Fatalf("filepath.Walk error: %v", err)
	}

	t.Logf("found %d golden tests", len(goldenTests))

	for _, goldenTest := range goldenTests {
		t.Run(fmt.Sprintf("file=%s, url=%s, name=%s", goldenTest.PrDomain.Id(), goldenTest.PrDomain.Url, goldenTest.PrDomain.Title), func(t *testing.T) {
			reExaminedPoints := *StandardPrPoints(goldenTest.PrDomain, goldenTest.CurrentUser, goldenTest.CurrentTime)
			want, got := goldenTest.Points, reExaminedPoints

			if diff := cmp.Diff(want, got); diff != "" {
				t.Logf("test: %s, name: %s", goldenTest.PrDomain.Url, goldenTest.PrDomain.Title)
				t.Errorf("StandardPrPoints() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func loadGoldenTest(filePath string) GoldenTest {
	jsonData, err := os.ReadFile(filePath)
	if err != nil {
		log.Fatalf("Failed to read golden test data from %s: %v", filePath, err)
	}

	var data GoldenTest
	err = json.Unmarshal(jsonData, &data)
	if err != nil {
		log.Fatalf("Failed to unmarshal golden test data from %s: %v", filePath, err)
	}

	return data
}
