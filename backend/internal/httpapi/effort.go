package httpapi

import "ai-factory/backend/internal/domain"

func (s *Server) effortOptions(model string) []string {
	provider := s.Config.ModelProviders[model]
	if provider == "" {
		provider = s.Config.AIProvider
	}
	if provider == "gemini" {
		return domain.GeminiEfforts(model)
	}
	return nil
}

func (s *Server) modelEfforts() map[string][]string {
	out := map[string][]string{}
	for _, model := range s.Config.Models {
		if levels := s.effortOptions(model); len(levels) > 0 {
			out[model] = levels
		}
	}
	return out
}
