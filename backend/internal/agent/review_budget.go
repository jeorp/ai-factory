package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"ai-factory/backend/internal/domain"
	"github.com/tiktoken-go/tokenizer"
)

const (
	ReviewMaxFiles        = 10
	ReviewDiffTokens      = 20000
	ReviewInputTokens     = 30000
	ReviewInputBytes      = 256 * 1024
	ReviewRequestBytes    = 512 * 1024
	ReviewMaxCalls        = 100
	ReviewRunInputTokens  = 500000
	ReviewRunOutputTokens = 64000
	ReviewRunDuration     = 15 * time.Minute
)

var reviewCodec = sync.OnceValue(func() tokenizer.Codec {
	// Embedded vocabulary; initialization needs no network or credentials.
	codec, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		panic(err)
	}
	return codec
})

// Shared planning estimate, not a claim about Claude/Gemini's exact tokenizer.
// Actual provider usage is used for the run budget and displayed separately.
func reviewTokens(text string) int {
	n, err := reviewCodec().Count(text)
	if err != nil {
		// One token per byte is a conservative fallback.
		return len(text)
	}
	return n
}

func reviewWorkflow(input string) bool {
	var body struct {
		Task *struct {
			Mode string `json:"mode"`
		} `json:"reviewTask"`
		Priority json.RawMessage `json:"priorityTask"`
	}
	if json.Unmarshal([]byte(input), &body) != nil {
		return false
	}
	return body.Task != nil && (body.Task.Mode == "diff" || body.Task.Mode == "cross") || len(body.Priority) > 0 && string(body.Priority) != "null"
}

func aiInputLimit(input string) int {
	if reviewWorkflow(input) {
		return ReviewInputBytes
	}
	return domain.MaxInputBytes
}

func aiRequestLimit(input string) int {
	if reviewWorkflow(input) {
		return ReviewRequestBytes
	}
	return 40 * 1024
}

func reviewInputFits(body []byte) bool {
	// Reserve 4K tokens for system instructions, schema and API framing.
	return len(body) <= ReviewInputBytes && reviewTokens(string(body))+4000 <= ReviewInputTokens
}

type reviewPatch struct {
	Path  string `json:"path"`
	Patch string `json:"patch"`
}

func reviewPatchesFit(patches []reviewPatch) bool {
	body, _ := json.Marshal(patches)
	return len(body) <= 128*1024 && reviewTokens(string(body)) <= ReviewDiffTokens
}

func reviewFiles(steps []domain.ReviewStep) []domain.ReviewFile {
	var files []domain.ReviewFile
	for _, step := range steps {
		files = append(files, step.Files...)
	}
	return files
}

// Pack complete lines and prefer a hunk/function boundary near the end of a
// large fragment. Never advance durable offsets until the batch is saved.
func nextReviewPatch(path, patch string, offset int, batch []reviewPatch) (reviewPatch, int) {
	left, right := patchCursor(patch[:offset])
	header := fmt.Sprintf("@@ -%d +%d @@\n", left, right)
	makePatch := func(end int) reviewPatch { return reviewPatch{Path: path, Patch: header + patch[offset:end]} }
	fits := func(end int) bool {
		candidate := append(append([]reviewPatch{}, batch...), makePatch(end))
		return reviewPatchesFit(candidate)
	}
	if fits(len(patch)) {
		return makePatch(len(patch)), len(patch)
	}
	ends := []int{}
	for pos := offset; pos < len(patch); {
		n := strings.IndexByte(patch[pos:], '\n')
		if n < 0 {
			ends = append(ends, len(patch))
			break
		}
		pos += n + 1
		ends = append(ends, pos)
	}
	lo, hi := 0, len(ends)
	for lo < hi {
		mid := (lo + hi) / 2
		if fits(ends[mid]) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		if len(batch) > 0 {
			return reviewPatch{}, offset // Flush and retry.
		}
		return reviewPatch{}, ends[0] // Explicitly report an oversized line.
	}
	end := ends[lo-1]
	preferred := end
	for pos := offset; pos < end; {
		n := strings.IndexByte(patch[pos:end], '\n')
		if n < 0 {
			break
		}
		next := pos + n + 1
		line := strings.TrimLeft(patch[pos:next], "+- ")
		if pos > offset+(end-offset)/2 && (strings.HasPrefix(line, "@@ ") || strings.HasPrefix(line, "func ") || strings.HasPrefix(line, "function ") || strings.HasPrefix(line, "export function ") || strings.HasPrefix(line, "def ") || strings.HasPrefix(line, "class ")) {
			preferred = pos
		}
		pos = next
	}
	// Token boundaries can merge at a new suffix, so recheck the preferred cut.
	if preferred < end && fits(preferred) {
		end = preferred
	}
	return makePatch(end), end
}
