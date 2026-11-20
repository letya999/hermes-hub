package devcheck

import (
	"strings"
	"testing"
)

// serviceTemplate mirrors the shape of services/README.template.md: the gate
// reads the required headings from the template itself, not from a Go list.
const serviceTemplate = "# <service>\n\n## Responsibility\n\ntext\n\n## Owns\n\n## Does not own\n"

const serviceReadmeComplete = "# x\n\n## Responsibility\n\nOne sentence.\n\n## Owns\n\n- a\n\n## Does not own\n\n- b\n"

// TestServiceReadmeTemplateHeadings: every services/<svc>/README.md must carry
// each "## " heading of services/README.template.md (M40.1, CHG-0334).
func TestServiceReadmeTemplateHeadings(t *testing.T) {
	t.Run("README with every template heading passes", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "services/README.template.md", serviceTemplate)
		gateWrite(t, root, "services/task-engine/README.md", serviceReadmeComplete)
		if err := Project(root); err != nil {
			t.Fatalf("complete service README rejected: %v", err)
		}
	})

	t.Run("README missing a template heading rejected", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "services/README.template.md", serviceTemplate)
		gateWrite(t, root, "services/task-engine/README.md", "# x\n\n## Responsibility\n\n## Owns\n")
		err := Project(root)
		if err == nil || !strings.Contains(err.Error(), "services/task-engine/README.md lacks heading \"## Does not own\"") {
			t.Fatalf("README without a template heading accepted: %v", err)
		}
	})

	t.Run("heading inside other text does not count", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "services/README.template.md", serviceTemplate)
		gateWrite(t, root, "services/task-engine/README.md",
			"# x\n\n## Responsibility\n\n## Owns\n\nsee ## Does not own below\n### Does not own\n")
		err := Project(root)
		if err == nil || !strings.Contains(err.Error(), "lacks heading \"## Does not own\"") {
			t.Fatalf("heading embedded in prose or at another level accepted: %v", err)
		}
	})

	t.Run("CRLF README passes", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "services/README.template.md", strings.ReplaceAll(serviceTemplate, "\n", "\r\n"))
		gateWrite(t, root, "services/task-engine/README.md", strings.ReplaceAll(serviceReadmeComplete, "\n", "\r\n"))
		if err := Project(root); err != nil {
			t.Fatalf("CRLF checkout rejected: %v", err)
		}
	})

	t.Run("README with an empty section rejected", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "services/README.template.md", serviceTemplate)
		gateWrite(t, root, "services/task-engine/README.md", "# x\n\n## Responsibility\n\nOne sentence.\n\n## Owns\n\n## Does not own\n\n- b\n")
		err := Project(root)
		if err == nil || !strings.Contains(err.Error(), "empty section \"## Owns\"") {
			t.Fatalf("README with an empty template section accepted: %v", err)
		}
	})

	t.Run("README with \"None.\" section passes", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "services/README.template.md", serviceTemplate)
		gateWrite(t, root, "services/task-engine/README.md", "# x\n\n## Responsibility\n\nOne sentence.\n\n## Owns\n\nNone.\n\n## Does not own\n\n- b\n")
		if err := Project(root); err != nil {
			t.Fatalf("None. section rejected: %v", err)
		}
	})

	t.Run("service without README rejected", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "services/README.template.md", serviceTemplate)
		gateWrite(t, root, "services/task-engine/main.go", "package main\n")
		err := Project(root)
		if err == nil || !strings.Contains(err.Error(), "missing services/task-engine/README.md") {
			t.Fatalf("service without README accepted: %v", err)
		}
	})

	t.Run("services without template rejected", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "services/task-engine/README.md", serviceReadmeComplete)
		err := Project(root)
		if err == nil || !strings.Contains(err.Error(), "missing services/README.template.md") {
			t.Fatalf("service tree without template accepted: %v", err)
		}
	})

	t.Run("template without headings rejected", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "services/README.template.md", "# <service>\n")
		gateWrite(t, root, "services/task-engine/README.md", serviceReadmeComplete)
		err := Project(root)
		if err == nil || !strings.Contains(err.Error(), "services/README.template.md has no ## headings") {
			t.Fatalf("empty template accepted: %v", err)
		}
	})

	t.Run("no services tree passes", func(t *testing.T) {
		if err := Project(gateRoot(t)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("exempt credential-broker README passes until M40.13", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "services/README.template.md", serviceTemplate)
		gateWrite(t, root, "services/credential-broker/README.md", "# Credential Broker\n\n## Что внутри\n")
		if err := Project(root); err != nil {
			t.Fatalf("documented exemption rejected: %v", err)
		}
	})

	t.Run("exempt README that follows the template must leave the exemption", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "services/README.template.md", serviceTemplate)
		gateWrite(t, root, "services/credential-broker/README.md", serviceReadmeComplete)
		err := Project(root)
		if err == nil || !strings.Contains(err.Error(), "stale README exemption: credential-broker") {
			t.Fatalf("stale exemption kept silently: %v", err)
		}
	})
}
