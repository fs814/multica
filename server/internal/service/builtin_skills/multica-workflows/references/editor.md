# Editor publication and run history

The editor publishes the revision and draft version the author reviewed. The UI
sends `revision` and `draft_version_id` together to the HTTP publish endpoint;
a changed revision or draft returns HTTP 409 and preserves the working graph.
Save-then-publish uses the save response's preconditions. Installed clients may
still send an empty publish body; the server reads, validates and publishes in
one transaction, but these legacy calls do not confirm a previously viewed draft.
This UI endpoint is not a new Action Contract action.

Canvas / Instances / Run history and list filters/pages are encoded in the URL.
Run history displays the run's immutable published graph and actual input keys.
Node badges reflect observed attempts; edges do not claim an executed path.
If the pinned version cannot be loaded, the linear attempt trace remains usable.
