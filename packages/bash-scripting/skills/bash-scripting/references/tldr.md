# Tldr Pages

Format from tldr-pages upstream. The builder discovers `<name>.md` in the script's `src` directory and installs it to `$out/share/tldr/pages.common/`.

Copy-ready template lives in `assets/tldr.md`.

```markdown
# command-name

> Short description.
> More information: <https://github.com/phillipgreenii/...>.

- Common usage:

`command-name {{arg}}`

- With flag:

`command-name --flag {{value}}`
```

Update the tldr page whenever common usage examples change.
