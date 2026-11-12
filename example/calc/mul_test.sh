# Unit tests of mul.
. ./lib.sh

test_mul() {
	[ "$(mul 6 7)" = 42 ] || fail "mul 6 7 = $(mul 6 7), want 42"
}

test_mul_zero() {
	[ "$(mul 9 0)" = 0 ] || fail "mul 9 0 = $(mul 9 0), want 0"
}
