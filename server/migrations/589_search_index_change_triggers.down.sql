DROP TRIGGER IF EXISTS trg_issue_search_index_change ON issue;
DROP TRIGGER IF EXISTS trg_comment_search_index_change ON comment;
DROP TRIGGER IF EXISTS trg_comment_update_search_index_change ON comment;
DROP TRIGGER IF EXISTS trg_project_search_index_change ON project;
DROP FUNCTION IF EXISTS record_search_index_change();
