package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// InstanceStart is an intent, never a caller-supplied saved snapshot. StartRun
// resolves and locks its authoritative row inside the issue/run transaction.
type InstanceStart struct {
	ID                pgtype.UUID
	Revision          int64
	Mode              string
	HistoryRunID      pgtype.UUID
	ProjectID         pgtype.UUID
	ImageAttachmentID *string
}

func SameInputSchema(a, b *Node) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.Key != b.Key || a.Type != b.Type || a.EffectiveInputMode() != b.EffectiveInputMode() || len(a.InputFields) != len(b.InputFields) {
		return false
	}
	for i, af := range a.InputFields {
		bf := b.InputFields[i]
		at, bt := af.Type, bf.Type
		if at == "" {
			at = "text"
		}
		if bt == "" {
			bt = "text"
		}
		if af.Key != bf.Key || at != bt || af.Label != bf.Label || af.Placeholder != bf.Placeholder || af.Required != bf.Required || !slices.Equal(af.Options, bf.Options) {
			return false
		}
	}
	return true
}

// ValidateInstanceInput is shared by readiness checks and actual execution.
// Unknown keys are refused so an explicit version upgrade cannot drop data.
func ValidateInstanceInput(ctx context.Context, q *db.Queries, ws pgtype.UUID, version db.WorkflowTemplateVersion, input []byte, project pgtype.UUID, image *string) error {
	def, err := ParseDefinition(version.Definition)
	if err != nil {
		return err
	}
	entry, ok := def.NodeByKey(def.EntryNode)
	if !ok {
		return newEngineError(ErrCodeInvalidSubmission, "workflow entry is unavailable")
	}
	var nullable map[string]*string
	if json.Unmarshal(input, &nullable) != nil || nullable == nil {
		return newEngineError(ErrCodeInvalidSubmission, "input must be a string-valued object")
	}
	for _, v := range nullable {
		if v == nil {
			return newEngineError(ErrCodeInvalidSubmission, "input values cannot be null")
		}
	}
	var values map[string]string
	if json.Unmarshal(input, &values) != nil || values == nil {
		return newEngineError(ErrCodeInvalidSubmission, "input must be a string-valued object")
	}
	title, description := strings.TrimSpace(values["title"]), strings.TrimSpace(values["description"])
	if title == "" || utf8.RuneCountInString(title) > 200 {
		return newEngineError(ErrCodeInvalidSubmission, "title is required and must be 200 characters or fewer")
	}
	if description == "" || utf8.RuneCountInString(description) > 20000 {
		return newEngineError(ErrCodeInvalidSubmission, "description is required and must be 20000 characters or fewer")
	}
	keys := map[string]bool{"title": true, "description": true}
	for _, f := range entry.InputFields {
		keys[f.Key] = true
	}
	for k := range values {
		if !keys[k] {
			return newEngineError(ErrCodeInvalidSubmission, fmt.Sprintf("input field %q is not declared in the pinned version; review it before running", k))
		}
	}
	if err := ValidateRunInput(input, entry); err != nil {
		return err
	}
	if project.Valid {
		if _, err := q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: project, WorkspaceID: ws}); err != nil {
			return newEngineError(ErrCodeInvalidSubmission, "project is unavailable in this workspace")
		}
	}
	if entry.EffectiveInputMode() == InputModeImage {
		id := entry.ImageAttachmentID
		if image != nil {
			id = *image
		}
		if _, err := resolveWorkflowImageAttachment(ctx, q, ws, id); err != nil {
			return err
		}
	}
	return nil
}

// ValidateInstanceImage also protects saving references without requiring a
// complete input form. An empty reference can be saved as an incomplete draft.
func ValidateInstanceImage(ctx context.Context, q *db.Queries, ws pgtype.UUID, id string) error {
	if id == "" {
		return nil
	}
	_, err := resolveWorkflowImageAttachment(ctx, q, ws, id)
	return err
}

func (e *Engine) resolveInstanceStart(ctx context.Context, q *db.Queries, in *StartRunInput) (*db.WorkflowInputInstance, error) {
	if in.Instance == nil {
		return nil, nil
	}
	intent := in.Instance
	row, err := q.LockWorkflowInputInstance(ctx, db.LockWorkflowInputInstanceParams{WorkspaceID: in.WorkspaceID, ID: intent.ID})
	if err != nil {
		return nil, newEngineError(ErrCodeNotFound, "input instance not found")
	}
	if row.TemplateID != in.TemplateID {
		return nil, newEngineError(ErrCodeNotFound, "input instance not found")
	}
	if row.ArchivedAt.Valid {
		return nil, newEngineError(ErrCodeInvalidTransition, "archived input instance cannot run")
	}
	if row.Revision != intent.Revision {
		return nil, newEngineError(ErrCodeIdempotencyConflict, "input instance changed; reload before running")
	}
	tpl, err := q.GetWorkflowTemplate(ctx, db.GetWorkflowTemplateParams{WorkspaceID: in.WorkspaceID, ID: row.TemplateID})
	if err != nil || tpl.Status == "archived" {
		return nil, newEngineError(ErrCodeInvalidTransition, "workflow is unavailable or archived")
	}
	in.TemplateVersionID = row.TemplateVersionID
	project := row.ProjectID
	image := row.ImageAttachmentID
	switch intent.Mode {
	case "saved":
		in.Input = row.Input
	case "temporary":
		project = intent.ProjectID
		if intent.ImageAttachmentID == nil {
			return nil, newEngineError(ErrCodeInvalidSubmission, "temporary input requires an explicit image reference (empty to clear)")
		}
		image = pgtype.Text{String: *intent.ImageAttachmentID, Valid: true}
	case "history":
		old, err := q.GetWorkflowRun(ctx, db.GetWorkflowRunParams{WorkspaceID: in.WorkspaceID, ID: intent.HistoryRunID})
		if err != nil || old.InputInstanceID != row.ID {
			return nil, newEngineError(ErrCodeNotFound, "instance run not found")
		}
		in.Input, in.TemplateVersionID, project = old.Input, old.TemplateVersionID, old.InputProjectID
		if old.InputInstanceRevision.Valid {
			row.Revision = old.InputInstanceRevision.Int64
		}
		if old.InputInstanceName.Valid {
			row.Name = old.InputInstanceName.String
		}
		image = pgtype.Text{Valid: true}
		if ref := imageAttachmentFromRunContext(old.Context); ref != nil {
			image.String = ref.ID
		}
	default:
		return nil, newEngineError(ErrCodeInvalidSubmission, "mode must be saved, temporary, or history")
	}
	if !in.TemplateVersionID.Valid {
		return nil, newEngineError(ErrCodeInvalidTransition, "instance needs an explicit published version binding")
	}
	version, err := e.resolveVersion(ctx, q, in.WorkspaceID, row.TemplateID, in.TemplateVersionID)
	if err != nil {
		return nil, err
	}
	var imageID *string
	if image.Valid {
		imageID = &image.String
	}
	if err := ValidateInstanceInput(ctx, q, in.WorkspaceID, version, in.Input, project, imageID); err != nil {
		return nil, err
	}
	in.Instance.ImageAttachmentID = imageID
	if in.Issue == nil {
		return nil, newEngineError(ErrCodeInvariantViolation, "instance run requires an issue")
	}
	var values map[string]string
	_ = json.Unmarshal(in.Input, &values)
	in.Issue.Title, in.Issue.Description, in.Issue.ProjectID = values["title"], values["description"], project
	return &row, nil
}
