# Checks that lib.sh defines every operation.
. ./lib.sh

test_operations_defined() {
	for op in add mul; do
		command -v "$op" >/dev/null || fail "lib.sh defines no $op"
	done
}
