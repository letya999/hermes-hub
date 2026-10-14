package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

func (s *Session) extract(ctx context.Context, rel string) (map[string]any, error) {
	if err := s.docs.enter(); err != nil {
		return nil, err
	}
	defer s.docs.leave()
	ctx, cancel := context.WithTimeout(ctx, DocumentTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	format, err := documentFormat(rel, true)
	if err != nil {
		return nil, err
	}
	body, err := s.readRegular(rel, MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	text, err := documentText(format, body)
	if err != nil {
		return nil, err
	}
	pages := pageCount(text)
	if pages > MaxPages {
		return nil, fmt.Errorf("document exceeds %d pages", MaxPages)
	}
	return map[string]any{
		"path": rel, "format": format, "text": text, "bytes": len(body),
		"pages": pages, "truncated": false, "secret_values_included": false,
	}, nil
}

func (s *Session) create(ctx context.Context, name, format, text string) (map[string]any, error) {
	if err := s.docs.enter(); err != nil {
		return nil, err
	}
	defer s.docs.leave()
	ctx, cancel := context.WithTimeout(ctx, DocumentTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	format = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), "."))
	if !slicesContains(DocumentFormats, format) {
		return nil, fmt.Errorf("unsupported document format %q", format)
	}
	if !validName(name) {
		return nil, errors.New("document name must be a short identifier")
	}
	body, plain, err := documentBody(format, text)
	if err != nil {
		return nil, err
	}
	rel := path.Join("artifacts", "documents", name+"."+format)
	if err := s.writeNew(rel, body); err != nil {
		return nil, err
	}
	return map[string]any{
		"path": rel, "format": format, "bytes": len(body), "pages": pageCount(plain),
		"secret_values_included": false,
	}, nil
}

func (s *Session) editDocument(ctx context.Context, rel, text string) (map[string]any, error) {
	if err := s.docs.enter(); err != nil {
		return nil, err
	}
	defer s.docs.leave()
	ctx, cancel := context.WithTimeout(ctx, DocumentTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(rel, "artifacts/documents/") {
		return nil, errors.New("only generated documents can be edited")
	}
	format, err := documentFormat(rel, false)
	if err != nil {
		return nil, err
	}
	if _, err = s.readRegular(rel, MaxDocumentBytes); err != nil {
		return nil, err
	}
	body, plain, err := documentBody(format, text)
	if err != nil {
		return nil, err
	}
	if err = s.writeReplacing(rel, body); err != nil {
		return nil, err
	}
	return map[string]any{
		"path": rel, "format": format, "bytes": len(body), "pages": pageCount(plain),
		"secret_values_included": false,
	}, nil
}

func (s *Session) convertDocument(ctx context.Context, rel, name, format string) (map[string]any, error) {
	if err := s.docs.enter(); err != nil {
		return nil, err
	}
	defer s.docs.leave()
	ctx, cancel := context.WithTimeout(ctx, DocumentTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	from, err := documentFormat(rel, true)
	if err != nil {
		return nil, err
	}
	format = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), "."))
	if !validName(name) {
		return nil, errors.New("document name must be a short identifier")
	}
	if from == format {
		return nil, errors.New("document format is unchanged")
	}
	if !slicesContains(DocumentFormats, format) {
		return nil, fmt.Errorf("unsupported document format %q", format)
	}
	if format == "csv" && from != "xlsx" {
		return nil, fmt.Errorf("cannot convert %s to csv", from)
	}
	raw, err := s.readRegular(rel, MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	plain, err := documentText(from, raw)
	if err != nil {
		return nil, err
	}
	body, plain, err := documentBody(format, plain)
	if err != nil {
		return nil, err
	}
	target := path.Join("artifacts", "documents", name+"."+format)
	if err = s.writeNew(target, body); err != nil {
		return nil, err
	}
	return map[string]any{
		"path": target, "format": format, "source": rel, "bytes": len(body), "pages": pageCount(plain),
		"secret_values_included": false,
	}, nil
}

func documentText(format string, body []byte) (string, error) {
	switch format {
	case "html":
		if !utf8.Valid(body) {
			return "", errors.New("document must be UTF-8")
		}
		return htmlText(string(body)), nil
	case "pdf":
		text, err := pdfText(body)
		if err == nil || strings.Contains(err.Error(), "encrypted") || strings.Contains(err.Error(), "exceeds") {
			return text, err
		}
		text, fallbackErr := popplerText(body)
		if fallbackErr != nil {
			return "", err
		}
		return text, nil
	case "xlsx":
		return xlsxText(body)
	case "pptx":
		return pptxText(body)
	default:
		if !utf8.Valid(body) {
			return "", errors.New("document must be UTF-8")
		}
		return string(body), nil
	}
}

func documentBody(format, text string) ([]byte, string, error) {
	if strings.Contains(text, "\x00") || !utf8.ValidString(text) {
		return nil, "", errors.New("document text must be UTF-8")
	}
	if len(text) > MaxDocumentBytes || containsSecret(text) {
		if containsSecret(text) {
			return nil, "", errors.New("refusing to store credential material")
		}
		return nil, "", fmt.Errorf("document exceeds %d bytes", MaxDocumentBytes)
	}
	if pageCount(text) > MaxPages {
		return nil, "", fmt.Errorf("document exceeds %d pages", MaxPages)
	}
	var body []byte
	var err error
	switch format {
	case "html":
		// The html format escapes plain text into an article shell (stored
		// artifacts must not carry active markup). Reject markup-shaped input
		// instead of storing a page of visible entities — file_write is the
		// tool for writing raw markup.
		if markupDocument(text) {
			return nil, "", errors.New("html documents escape the supplied text for safety; pass plain text, or write markup with file_write")
		}
		body = []byte(htmlDocument(text))
	case "pdf":
		body, err = pdfBytes(text)
	case "xlsx":
		body, err = xlsxBytes(text)
	case "pptx":
		body, err = pptxBytes(text)
	default:
		body = []byte(text)
	}
	if err != nil {
		return nil, "", err
	}
	if len(body) > MaxDocumentBytes {
		return nil, "", fmt.Errorf("document exceeds %d bytes", MaxDocumentBytes)
	}
	if containsSecretBytes(body) {
		return nil, "", errors.New("refusing to store credential material")
	}
	return body, text, nil
}

func htmlDocument(text string) string {
	return "<!DOCTYPE html>\n<meta charset=\"utf-8\">\n<article>\n" + htmlEscape(text) + "\n</article>\n"
}

// markupDocument reports whether the supplied text is markup rather than
// plain text: a full document marker anywhere, or a tag fragment with a
// closing/self-closing pair. Such input belongs in file_write, not the
// escaped-article html format.
func markupDocument(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, marker := range []string{"<!doctype", "<html", "<head", "<body"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return strings.HasPrefix(lower, "<") && (strings.Contains(lower, "</") || strings.Contains(lower, "/>"))
}

func (s *Session) remove(rel string) (map[string]any, error) {
	if err := relPath(rel); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(rel, "artifacts/documents/") && !strings.HasPrefix(rel, "artifacts/images/") && !strings.HasPrefix(rel, "artifacts/videos/") {
		return nil, errors.New("only generated artifacts can be removed")
	}
	if path.Base(rel) == "." || strings.HasSuffix(rel, "/") {
		return nil, errors.New("regular file required")
	}
	info, err := s.Workspace.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("regular file required")
	}
	if err := s.Workspace.Remove(rel); err != nil {
		return nil, err
	}
	return map[string]any{"path": rel, "removed": true, "secret_values_included": false}, nil
}

func documentFormat(rel string, extract bool) (string, error) {
	if err := relPath(rel); err != nil {
		return "", err
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(rel), "."))
	if slicesContains(UnsupportedDocuments, ext) || slicesContains(UnsupportedImages, ext) {
		return "", fmt.Errorf("unsupported document format %q", ext)
	}
	if ext == "htm" && extract {
		return "html", nil
	}
	if !slicesContains(DocumentFormats, ext) {
		return "", fmt.Errorf("unsupported document format %q", ext)
	}
	return ext, nil
}
