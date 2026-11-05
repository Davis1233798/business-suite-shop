# Print preview resource lifecycle

The workspace print preview temporarily loads the back-office dataset into the parent React app. That copy exists only while the print preview is open. Closing the preview clears the copy and aborts its in-flight request; leaving the page or changing the eligible workspace scope also aborts the request. A response or error from an aborted request must not update UI state.

This boundary applies only to the print-preview dataset. It does not clear the active workspace dataset, user-entered form drafts, or authentication state. The iframe previews are separately mounted only while selected and are mutually exclusive.

The Playwright lifecycle regression opens print preview while its dataset request is held, closes the preview, checks that the browser reports the request as aborted, then reopens and verifies that a fresh dataset request succeeds. CI runs it against the isolated SHOP station image.
