#!/bin/sh
# Removes Aegis Agent; add --purge to delete its settings and history as well.
exec sh "$(dirname "$0")/install.sh" --uninstall "$@"
