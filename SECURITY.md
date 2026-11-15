# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab, "Report
a vulnerability". Don't open a public issue for it.

Say what is affected (pipelinegen or a plugin, and the version), how it can
be used, and how to reproduce it.

## What counts

pipelinegen runs in CI with a token that can write to your repository, and
it runs plugins on your code. Reports we particularly want:

- a way for a pull request, especially one from a fork, to get the token
  or to run its own code with it;
- a token or other secret reaching a log, a URL, a commit status, the
  comment, or the repository's refs;
- a plugin download that runs without its checksum being checked;
- a way to make a plan select fewer tests than a change reaches without
  the plan saying so.
