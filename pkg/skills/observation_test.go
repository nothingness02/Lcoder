package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillLoadingObservation prints a markdown report of how skills appear in
// context under the Pi-style lazy loading design. It is a human-readable
// observation test, not an assertion test.
func TestSkillLoadingObservation(t *testing.T) {
	dir, err := os.MkdirTemp("", "lcoder-skill-observation-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	writeSkill(t, dir, "security-review", `---
name: security-review
description: Review code for security vulnerabilities, injection risks, authentication, and authorization problems.
keywords:
  - security
  - review
  - vulnerability
  - injection
---

# Security Review

Use this skill when the user asks for a security review of code, configuration, or architecture.

## Steps

- Read the requested file(s) in full.
- Identify input validation, authentication, and injection risks.
- Report findings with severity and suggested fixes.

## Examples

- "Review auth.ts for security issues"
- "Check this API for SQL injection"
`)

	writeSkill(t, dir, "refactor", `---
name: refactor
description: Refactor code following project conventions and best practices.
keywords:
  - refactor
  - cleanup
  - improve
---

# Refactor

Improve the provided code without changing its external behavior.

## Steps

- Identify duplication and unclear naming.
- Apply small, safe transformations.
- Keep tests passing.
`)

	catalog, err := LoadCatalog([]string{dir})
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	var sb strings.Builder
	sb.WriteString("# Skill 加载机制上下文观察\n\n")
	sb.WriteString("## 1. 磁盘上的 skill 文件\n\n")
	sb.WriteString("目录结构：\n\n")
	sb.WriteString("```text\n")
	sb.WriteString(dir + "\n")
	sb.WriteString("├── security-review/SKILL.md\n")
	sb.WriteString("└── refactor/SKILL.md\n")
	sb.WriteString("```\n\n")

	sb.WriteString("## 2. 启动后加载的 catalog（轻量元数据）\n\n")
	sb.WriteString("每个 skill 只解析 frontmatter，不读取正文：\n\n")
	sb.WriteString("```go\n")
	for _, m := range catalog {
		fmt.Fprintf(&sb, "SkillMeta{Name: %q, Description: %q, Keywords: %v, Source: %q}\n", m.Name, m.Description, m.Keywords, m.Source)
	}
	sb.WriteString("```\n\n")

	sb.WriteString("## 3. 注入系统提示的目录块\n\n")
	sb.WriteString("这是启动后常驻上下文的内容：\n\n")
	sb.WriteString("```text\n")
	sb.WriteString(ToCatalogBlock(catalog))
	sb.WriteString("\n```\n\n")

	sb.WriteString("## 4. 用户触发 `/skill:security-review`\n\n")
	meta, ok := FindByName(catalog, "security-review")
	if !ok {
		t.Fatal("security-review not found")
	}
	skill, err := LoadSkill(meta.Source)
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}

	sb.WriteString("命中后调用 `LoadSkill` 读取完整 `SKILL.md`：\n\n")
	sb.WriteString("```go\n")
	fmt.Fprintf(&sb, "Skill{Name: %q, Description: %q, Body: %q}\n", skill.Name, skill.Description, truncate(skill.Body, 120))
	sb.WriteString("```\n\n")

	sb.WriteString("## 5. 注入对话的上下文\n\n")
	sb.WriteString("`ExpandManualTrigger` 生成两条消息，追加到当前会话：\n\n")
	expanded := ExpandManualTrigger(skill, "check auth.go")
	for i, msg := range expanded {
		role := msg.Role
		fmt.Fprintf(&sb, "### Message %d (%s)\n\n", i+1, role)
		sb.WriteString("```text\n")
		sb.WriteString(msg.Text())
		sb.WriteString("\n```\n\n")
	}

	sb.WriteString("## 6. 关键观察\n\n")
	sb.WriteString("- **未激活时**：系统提示只包含 `name + description + keywords`，不会把 security-review 或 refactor 的完整正文带进去。\n")
	sb.WriteString("- **激活后**：完整正文以 system/user 消息对的形式进入**动态对话**，不占静态 system prompt。\n")
	sb.WriteString("- **切换 skill**：只需替换注入的消息，不会使静态 system prompt 缓存失效。\n")
	sb.WriteString("- **多 skill 共存**：同一个会话可以先后激活多个 skill，每个都以独立消息对注入。\n")

	fmt.Println(sb.String())
}

func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
