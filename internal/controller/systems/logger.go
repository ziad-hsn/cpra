package systems

import "go.uber.org/zap"

// Logger is the printf-style logger used by systems.
// It is an alias for zap.SugaredLogger which provides methods like
// Infof, Debugf, Warnf, Errorf, and Fatalf for formatted logging.
type Logger = *zap.SugaredLogger
