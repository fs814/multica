package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// maxWorkflowImageBytes mirrors the client upload limit. It is enforced here as
// well because an API caller can bypass the browser before a Run exists.
const maxWorkflowImageBytes int64 = 100 * 1024 * 1024

// ImageAttachmentRef is the small, durable reference an agent needs to fetch a
// workflow image. It intentionally has no storage URL, signed URL, or image
// bytes: attachments are re-authorized at download time by the CLI endpoint.
type ImageAttachmentRef struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

type workflowRunContext struct {
	ImageAttachment *ImageAttachmentRef `json:"image_attachment,omitempty"`
}

func isWorkflowImageContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch strings.ToLower(mediaType) {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

// resolveWorkflowImageAttachment verifies the attachment selected by the pinned
// workflow definition before a Run is created. Publishing is admin-gated, so the
// definition is the authority; the member pressing Run does not need to be the
// original uploader. The workspace check still prevents a definition from
// smuggling an attachment across tenant boundaries.
func resolveWorkflowImageAttachment(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, attachmentID string) (*ImageAttachmentRef, error) {
	id, err := util.ParseUUID(strings.TrimSpace(attachmentID))
	if err != nil {
		return nil, newEngineError(ErrCodeInvalidSubmission, "image attachment id is invalid")
	}
	attachment, err := q.GetAttachment(ctx, db.GetAttachmentParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Keep absent, deleted, and cross-workspace IDs indistinguishable.
			return nil, newEngineError(ErrCodeInvalidSubmission, "image attachment was not found")
		}
		return nil, fmt.Errorf("load image attachment: %w", err)
	}
	if !isWorkflowImageContentType(attachment.ContentType) {
		return nil, newEngineError(ErrCodeInvalidSubmission, "image attachment must be a JPEG, PNG, WebP, or GIF")
	}
	if attachment.SizeBytes > maxWorkflowImageBytes {
		return nil, newEngineError(ErrCodeInvalidSubmission, "image attachment exceeds the 100 MB limit")
	}
	return &ImageAttachmentRef{
		ID:          uuidString(attachment.ID),
		Filename:    attachment.Filename,
		ContentType: attachment.ContentType,
	}, nil
}

func marshalWorkflowRunContext(image *ImageAttachmentRef) ([]byte, error) {
	if image == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(workflowRunContext{ImageAttachment: image})
}

func imageAttachmentFromRunContext(raw []byte) *ImageAttachmentRef {
	var context workflowRunContext
	if len(raw) == 0 || json.Unmarshal(raw, &context) != nil {
		return nil
	}
	if context.ImageAttachment == nil || context.ImageAttachment.ID == "" {
		return nil
	}
	return context.ImageAttachment
}
