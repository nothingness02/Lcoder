// Package bpe embeds the BPE vocabulary files used by the token estimator.
//
// The files are OpenAI's official tiktoken vocabularies, shipped inside the
// binary so token estimation never needs network access (the default
// tiktoken-go loader would otherwise fetch them from openaipublic.blob.core
// .windows.net on first use).
package bpe

import "embed"

//go:embed o200k_base.tiktoken cl100k_base.tiktoken
var Files embed.FS
