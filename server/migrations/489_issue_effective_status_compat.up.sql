-- Fresh databases on this delivery line do not yet have the custom-status
-- helper used by WorkflowRun. Preserve an existing implementation on upgraded
-- databases and install a compatibility implementation only when it is absent.
DO $do$
BEGIN
    IF to_regprocedure('public.issue_effective_status(uuid,text)') IS NULL THEN
        EXECUTE $create$
            CREATE FUNCTION public.issue_effective_status(p_workspace_id uuid, p_status text)
            RETURNS text
            LANGUAGE plpgsql
            STABLE
            PARALLEL SAFE
            AS $body$
            DECLARE
                v_category text;
            BEGIN
                IF p_status IN ('backlog', 'todo', 'in_progress', 'in_review', 'done', 'blocked', 'cancelled') THEN
                    RETURN p_status;
                END IF;

                IF to_regclass('public.issue_status') IS NOT NULL THEN
                    EXECUTE
                        'SELECT category FROM public.issue_status WHERE workspace_id = $1 AND key = $2'
                        INTO v_category
                        USING p_workspace_id, p_status;
                    IF v_category IN ('backlog', 'todo', 'in_progress', 'in_review', 'done', 'blocked', 'cancelled') THEN
                        RETURN v_category;
                    END IF;
                END IF;

                RETURN p_status;
            END
            $body$
        $create$;
        COMMENT ON FUNCTION public.issue_effective_status(uuid, text)
            IS 'tes66-migration-489-compatibility-helper';
    END IF;
END
$do$;
