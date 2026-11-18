# WORDS is the address of the server the batch started.

test_upper() {
	got=$(wget -qO- "$WORDS/upper?s=hey+you")
	[ "$got" = "HEY YOU" ] || fail "/upper?s=hey+you = $got, want HEY YOU"
}
