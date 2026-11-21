# Releasing the CLI

The `mailgoose` CLI is built from this repository by GoReleaser
(`.goreleaser.yaml`) and published by the "Release CLI" workflow
(`.github/workflows/release-cli.yml`) to:

- **GitHub Releases** on the public repository
  [gremlin-technologies/mailgoose-public](https://github.com/gremlin-technologies/mailgoose-public/releases):
  a `.tar.gz` for macOS and Linux and a `.zip` for Windows (amd64 and arm64),
  `.deb` and `.rpm` packages for Linux, and `checksums.txt`. File names carry
  no version, so `releases/latest/download/<file>` always gets the newest.
- **Homebrew**: a cask in
  [gremlin-technologies/homebrew-tap](https://github.com/gremlin-technologies/homebrew-tap),
  for macOS and Linux.
- **The AUR**: the package `mailgoose-bin`, for Arch (pacman via an AUR
  helper).

Both files are on the owner-merge list: a PR that changes either is reviewed
as usual but left for the owner to merge (see `CLAUDE.md`).

Archives hold only the binary; nothing else from this repository is published.
The binaries aren't signed or notarized yet, so the Homebrew cask clears
macOS's quarantine flag on the binary it installs.

## Cutting a release

Actions > **Release CLI** > Run workflow, with a version like `v0.1.0`. (Or
push a tag like `v0.1.0` to this repository.) A suffix like `-beta.1` marks the
release as a pre-release, which the `latest` download links and Homebrew skip,
so use plain versions for anything testers install.

`make release-snapshot` builds everything locally into `dist/` without
publishing (needs GoReleaser v2).

## Install commands (once a release exists)

```sh
# Homebrew (macOS, Linux)
brew install gremlin-technologies/tap/mailgoose

# Arch, with an AUR helper such as yay or paru
yay -S mailgoose-bin

# Debian, Ubuntu (use _arm64 on ARM)
curl -fsSLO https://github.com/gremlin-technologies/mailgoose-public/releases/latest/download/mailgoose_linux_amd64.deb
sudo apt install ./mailgoose_linux_amd64.deb

# Fedora, RHEL
sudo dnf install https://github.com/gremlin-technologies/mailgoose-public/releases/latest/download/mailgoose_linux_amd64.rpm

# Any macOS or Linux, by hand (pick your OS and arch)
curl -fsSL https://github.com/gremlin-technologies/mailgoose-public/releases/latest/download/mailgoose_linux_amd64.tar.gz | tar -xz mailgoose
sudo install mailgoose /usr/local/bin/
```

On Windows, download `mailgoose_windows_amd64.zip` from the latest release and
put `mailgoose.exe` on your `PATH`.

## One-time setup (the owner)

1. **Homebrew tap repository.** Create a public repository named
   `homebrew-tap` under gremlin-technologies, with a README (so it has a
   `main` branch). The name must start with `homebrew-` for
   `brew install gremlin-technologies/tap/...` to find it.
2. **Release token.** Create a fine-grained personal access token
   (GitHub > Settings > Developer settings > Fine-grained tokens > Generate):
   - Resource owner: gremlin-technologies.
   - Repository access: only `mailgoose-public` and `homebrew-tap`.
   - Repository permissions: **Contents: Read and write** (Metadata read-only
     is added automatically). Nothing else.
   - Expiration: up to a year; note the date, since releases fail once it
     expires.
   Save it in this repository as the Actions secret `RELEASE_TOKEN`
   (Settings > Secrets and variables > Actions > New repository secret).
3. **AUR (optional; without it the AUR step is skipped).**
   - Register an account at aur.archlinux.org.
   - Make a key for it, on your own machine:
     `ssh-keygen -t ed25519 -C "mailgoose AUR" -f ~/.ssh/aur_mailgoose -N ""`.
   - Paste the public half (`~/.ssh/aur_mailgoose.pub`) into the AUR's
     My Account > SSH Public Key.
   - Save the private half (`~/.ssh/aur_mailgoose`) in this repository as the
     Actions secret `AUR_KEY`. The first release creates the `mailgoose-bin`
     package under that account.

Without `RELEASE_TOKEN` nothing can be published. Without `AUR_KEY` the release
and the Homebrew cask still go out.

## Not yet

- **Scoop (Windows) and winget:** each needs its own bucket or manifest
  repository; GoReleaser can add them later in a few lines.
- **Signing and notarizing** the macOS binaries, so the quarantine workaround
  can go.
- **A licence file.** The packages say "custom" / "Proprietary" for now.
