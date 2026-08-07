---
name: security-review
description: Review code for security vulnerabilities, injection risks, authentication, and authorization problems.
keywords:
  - security
  - review
  - vulnerability
  - injection
  - authentication
  - authorization
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

## Output format

- Summary
- Findings (severity + description + fix)
