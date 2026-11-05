# Print preview resource lifecycle

The workspace print preview temporarily loads the back-office dataset into the parent React app. That copy exists only while the print preview is open. Closing the preview clears the copy and aborts its in-flight request; leaving the page or changing the eligible workspace scope also aborts the request. A response or error from an aborted request must not update UI state.

This boundary applies only to the print-preview dataset. It does not clear the active workspace dataset, user-entered form drafts, or authentication state. The iframe previews are separately mounted only while selected and are mutually exclusive.

The lifecycle contract is covered by `tests/unit/print-preview-lifecycle.test.ts` in the existing CI unit-test command.
