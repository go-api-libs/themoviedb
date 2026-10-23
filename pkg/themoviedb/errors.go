package themoviedb

import (
	"fmt"
)

func (a *Error) Error() string {
	return fmt.Sprintf("%d %s", a.StatusCode, a.StatusMessage)
}
