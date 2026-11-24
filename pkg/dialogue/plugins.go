package dialogue

// The plugins StartPlugins can start, registered by their packages: here, so a program embedding
// the dialogue has them (their packages are internal to fsttm-live).
import (
	_ "github.com/ibnHatab/fsttm-live/internal/plugins/gpio"
	_ "github.com/ibnHatab/fsttm-live/internal/plugins/web"
)
