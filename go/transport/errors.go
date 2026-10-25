package transport

import "errors"

// ErrLineTooLong is returned when a single stdio message exceeds
// MaxLineBytes. It is deliberately a distinct error from io.EOF so the caller
// can tell "peer went away" from "peer sent something absurd".
var ErrLineTooLong = errors.New("transport: message exceeds line limit")
