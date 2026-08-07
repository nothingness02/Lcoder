package contextmgr

import (
	"encoding/base64"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/lcoder/lcoder/pkg/contextmgr/bpe"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/pkoukk/tiktoken-go"
)

// embedBpeLoader serves the BPE vocabularies from the embedded filesystem so
// token estimation never needs network access. tiktoken-go's default loader
// fetches vocabularies from openaipublic.blob.core.windows.net on first use,
// which breaks offline use and the single-binary distribution.
type embedBpeLoader struct{}

func (embedBpeLoader) LoadTiktokenBpe(file string) (map[string]int, error) {
	data, err := bpe.Files.ReadFile(filepath.Base(file))
	if err != nil {
		return nil, err
	}
	ranks := make(map[string]int, 100256)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line) // CRLF checkouts leave a trailing \r
		if line == "" {
			continue
		}
		parts := strings.Split(line, " ")
		if len(parts) != 2 {
			continue
		}
		token, err := base64.StdEncoding.DecodeString(parts[0])
		if err != nil {
			return nil, err
		}
		token = []byte(strings.TrimSuffix(string(token), "\r"))
		rank, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, err
		}
		ranks[string(token)] = rank
	}
	return ranks, nil
}

var (
	loaderOnce sync.Once
	// encCache caches Tiktoken instances: GetEncoding recompiles the BPE
	// regexp every call, which is expensive (regexp2 compile of the full
	// pattern ~ms). Estimation runs at compaction/state boundaries, so the
	// first call pays the cost once per encoding.
	encCache sync.Map // encoding name -> *tiktoken.Tiktoken
)

// getTiktoken returns a cached Tiktoken for the encoding name.
func getTiktoken(encoding string) (*tiktoken.Tiktoken, error) {
	loaderOnce.Do(func() { tiktoken.SetBpeLoader(embedBpeLoader{}) })
	if v, ok := encCache.Load(encoding); ok {
		return v.(*tiktoken.Tiktoken), nil
	}
	enc, err := tiktoken.GetEncoding(encoding)
	if err != nil {
		return nil, err
	}
	encCache.Store(encoding, enc)
	return enc, nil
}

// encodingForModel resolves the BPE encoding for a model id with three tiers:
//  1. exact/prefix match in tiktoken's model table (gpt-4o, gpt-4.1, ...)
//  2. naming-pattern inference for common non-OpenAI families (claude,
//     deepseek, kimi, qwen, ...) → cl100k_base (the standard proxy for models
//     whose tokenizer is not public)
//  3. fallback: any unknown modern LLM → cl100k_base, NOT the char heuristic
//     (a BPE approximation is always closer to the true count than
//     4-chars-per-token)
func encodingForModel(modelID string) string {
	if name, ok := tiktoken.MODEL_TO_ENCODING[modelID]; ok {
		return name
	}
	for prefix, name := range tiktoken.MODEL_PREFIX_TO_ENCODING {
		if strings.HasPrefix(modelID, prefix) {
			return name
		}
	}
	lower := strings.ToLower(modelID)
	if strings.Contains(lower, "gpt-5") || strings.Contains(lower, "o1") || strings.Contains(lower, "o3") || strings.Contains(lower, "o4") {
		return tiktoken.MODEL_O200K_BASE
	}
	// claude / deepseek / kimi / qwen / llama / mistral / gemini and any
	// unknown id: cl100k_base is the standard stand-in.
	return tiktoken.MODEL_CL100K_BASE
}

// TiktokenEstimator builds a TokenEstimator bound to a model id. It resolves
// the model's BPE encoding (see encodingForModel) and counts tokens across all
// content parts including thinking traces. Falls back to DefaultEstimator only
// when the embedded vocabulary cannot be loaded (corrupt binary).
func TiktokenEstimator(modelID string) TokenEstimator {
	enc, err := getTiktoken(encodingForModel(modelID))
	if err != nil {
		return DefaultEstimator
	}
	return func(messages []models.AgentMessage) int {
		total := 0
		for _, m := range messages {
			for _, part := range m.Content {
				switch p := part.(type) {
				case models.TextContent:
					total += len(enc.Encode(p.Text, nil, nil))
				case models.ThinkingContent:
					total += len(enc.Encode(p.Text, nil, nil))
				}
			}
		}
		return total
	}
}
