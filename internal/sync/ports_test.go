package sync

import "github.com/amasok/asagao/internal/asana"

// The Asana client satisfies the port as it is.
var _ AsanaAPI = (*asana.Client)(nil)
