package notes

import "errors"

// errSkipNote means the path is not a regular file inside the notes directory.
var errSkipNote = errors.New("skip note")
