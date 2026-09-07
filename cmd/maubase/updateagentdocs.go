package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// managedBlockPattern matches a whole <!-- maubase:begin ... --> ...
// <!-- maubase:end --> block, version marker and all — see
// agentDocBody's doc comment for why it exists.
var managedBlockPattern = regexp.MustCompile(`(?s)<!-- maubase:begin.*?<!-- maubase:end -->`)

// runUpdateAgentDocs implements `maubase init --update-agent-docs [dir]`
// — see spec/project-init.md INIT-09. Unlike a plain `maubase init`, it
// never refuses over files that already exist: that's the whole point,
// re-running it after upgrading the maubase binary is how the skill and
// AGENTS.md's version marker and spec links move to match. It doesn't
// touch migrations/, .env.example, or .gitignore at all — those aren't
// versioned content the way the agent-context files are.
func runUpdateAgentDocs(dir string) error {
	jsClient := hasJSClient(dir)

	skillPath := filepath.Join(dir, skillRelPath)
	skillStatus, err := updateManagedBlock(skillPath, renderSkill(jsClient))
	if err != nil {
		return err
	}
	fmt.Printf("%s %s\n", skillStatus, skillPath)

	agentsPath := filepath.Join(dir, agentsRelPath)
	agentsStatus, err := updateManagedBlock(agentsPath, renderAgentsDoc(jsClient))
	if err != nil {
		return err
	}
	fmt.Printf("%s %s\n", agentsStatus, agentsPath)

	return nil
}

// updateManagedBlock brings path's managed block in line with rendered:
//
//   - path doesn't exist yet: rendered is written in full, same as a
//     fresh `maubase init` would (this is also how a project that
//     predates this feature entirely picks it up, with no separate
//     migration step).
//   - path exists and has a <!-- maubase:begin -->...<!-- maubase:end -->
//     block: only that block is replaced with rendered's own block —
//     everything before it (skillTemplate's YAML frontmatter) and
//     everything after it (anything a person appended below
//     <!-- maubase:end -->) is left exactly as it was.
//   - path exists but has no such block (a person deleted or rewrote it
//     by hand): refuses rather than guessing where regenerated content
//     belongs in a file it no longer recognizes.
func updateManagedBlock(path, rendered string) (status string, err error) {
	newBlock := managedBlockPattern.FindString(rendered)
	if newBlock == "" {
		return "", fmt.Errorf("internal error: rendered template for %s has no managed block", path)
	}

	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", path, err)
		}
		return "created", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	if !managedBlockPattern.MatchString(string(existing)) {
		return "", fmt.Errorf("%s has no maubase:begin/maubase:end managed block — not touching it; restore the block by hand, or delete the file and re-run to scaffold it fresh", path)
	}

	updated := managedBlockPattern.ReplaceAllLiteralString(string(existing), newBlock)
	if updated == string(existing) {
		return "unchanged", nil
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return "updated", nil
}
