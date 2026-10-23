package themoviedb

import (
	"fmt"
)

func (a *AuthenticationValidateKey) Error() string {
	return fmt.Sprintf("%d %s", a.StatusCode, a.StatusMessage)
}
