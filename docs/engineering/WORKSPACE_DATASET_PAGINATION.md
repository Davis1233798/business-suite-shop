# Workspace dataset table pagination

Only workspace dataset tables use client-side pagination at 50 rows per page over the existing loaded, organization-authorized result set. The API query cap and authorization remain unchanged. Search filters loaded rows and returns to page one when its result set changes.

PrintCenter documents remain complete and unpaginated, including long line lists. Compact summaries remain capped at five rows. Pagination does not change form drafts, auth state, or business API behavior.

The browser fixture covers 500 workspace rows, first/next/last navigation, search reset, preview-state retention, Traditional Chinese labels, 390px viewport width, and a 675-line print document.

TanStack Table v9 client-side pagination uses useTable, tableFeatures, rowPaginationFeature, and createPaginatedRowModel. See the official pagination guide https://tanstack.com/table/latest/docs/framework/react/guide/pagination and example https://tanstack.com/table/latest/docs/framework/react/examples/pagination.
