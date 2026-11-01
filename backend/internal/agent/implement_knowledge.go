package agent

import (
	_ "embed"
	"path"
	"sort"
	"strings"
)

// The catalog is application-owned, versioned with the backend and available
// without another service or repository/network read during verification.
//
//go:embed verification_knowledge/common.md
var commonVerificationKnowledge string

//go:embed verification_knowledge/go.md
var goVerificationKnowledge string

//go:embed verification_knowledge/javascript-typescript.md
var javascriptVerificationKnowledge string

//go:embed verification_knowledge/python.md
var pythonVerificationKnowledge string

type languageVerificationKnowledge struct {
	Language string   `json:"language"`
	Paths    []string `json:"paths"`
	Guidance string   `json:"guidance"`
}

type verificationKnowledge struct {
	Version   string                          `json:"version"`
	Common    string                          `json:"common"`
	Languages []languageVerificationKnowledge `json:"languages"`
}

var verificationKnowledgeCatalog = []struct {
	language   string
	extensions []string
	guidance   *string
}{
	{"go", []string{".go"}, &goVerificationKnowledge},
	{"javascript-typescript", []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts"}, &javascriptVerificationKnowledge},
	{"python", []string{".py", ".pyi"}, &pythonVerificationKnowledge},
}

func knowledgeForVerification(paths []string) *verificationKnowledge {
	result := &verificationKnowledge{Version: "language-verification/v1", Common: strings.TrimSpace(commonVerificationKnowledge)}
	for _, entry := range verificationKnowledgeCatalog {
		matched := map[string]bool{}
		for _, p := range paths {
			ext := strings.ToLower(path.Ext(p))
			for _, candidate := range entry.extensions {
				if ext == candidate {
					matched[p] = true
					break
				}
			}
		}
		if len(matched) == 0 {
			continue
		}
		selected := make([]string, 0, len(matched))
		for p := range matched {
			selected = append(selected, p)
		}
		sort.Strings(selected)
		result.Languages = append(result.Languages, languageVerificationKnowledge{Language: entry.language, Paths: selected, Guidance: strings.TrimSpace(*entry.guidance)})
	}
	if len(result.Languages) == 0 {
		return nil
	}
	return result
}
