package bashrisk

// RiskLevel indicates the danger of a bash command.
type RiskLevel int

const (
	RiskNone RiskLevel = iota
	RiskLow
	RiskHigh
)

// Category describes why a command is risky.
type Category string

const (
	CatFileWriteOutside Category = "file-write-outside"
	CatFileDelete       Category = "file-delete"
	CatNetwork          Category = "network"
	CatExternalCode     Category = "external-code"
	CatPrivilege        Category = "privilege"
	CatCredential       Category = "credential"
	CatPackageInstall   Category = "package-install"
)

// Report is the result of classifying a command.
type Report struct {
	Level      RiskLevel
	Categories []Category
}

// Classify analyzes command in the context of projectRoot.
func Classify(command, projectRoot string) Report {
	if command == "" {
		return Report{Level: RiskNone}
	}
	tokens := tokenize(command)
	cats := detectCategories(tokens, projectRoot)
	level := RiskNone
	if len(cats) > 0 {
		level = RiskHigh
	}
	return Report{Level: level, Categories: cats}
}

func tokenize(s string) []string {
	// Placeholder: simple fields split.
	return []string{s}
}

func detectCategories(tokens []string, projectRoot string) []Category {
	// Placeholder for Task 3.
	return nil
}
