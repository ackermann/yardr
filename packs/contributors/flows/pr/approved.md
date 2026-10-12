summary: A reviewed pull request waits here for a maintainer, who merges it on GitHub or closes it.

A pull request the reviewer found ready waits here for a maintainer. No
agent works this stage. The review is a note on the bead; it was made by
reading, and nothing of the pull request has been run by the yard.

Running it is here, and a person's: a maintainer who wants to see it work
pulls the code and runs it where they choose, by their own judgement of
the author and of what the review says. The repository's CI is the other
place it runs, under the repository's own rules for outside pull requests.

What of the review reaches the contributor is the maintainer's to post, in
their own words or the note's, approved text only: `yardr reply --crew
yardmaster <seq> - < review.md`, where `<seq>` is the number the pull
request's delivery starts with in the inbox and review.md holds the
approved text. The command's environment carries the crew's credential,
the GitHub App's GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_FILE as the
yardmaster's env_file sets them, so the comment goes out under the yard's
bot, the App's `<slug>[bot]`; at a bare shell with neither them nor
GITHUB_TOKEN set, it goes out as the shell's gh login instead. Nothing
posts by itself: no reviewer, no stage and no hook puts the review on the
pull request.

A write through the yard's GitHub App has not been proven on every yard:
check the comment appeared on the pull request.

The bead leaves on what the maintainer does on GitHub. Nothing of the yard
merges a pull request or pushes to one:

- `merged`: the pull request is merged. The bead goes to `merged`.
- `dropped`: it is closed unmerged. The bead goes to `closed`.

Or by hand: `yardr bead advance <id> --to merged`, or `--to closed`.
