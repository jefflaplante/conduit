# Markdown Cheat Sheet

A quick reference for all the formatting you can do in Markdown.

---

## Headers

```markdown
# H1 - Largest
## H2
### H3
#### H4
##### H5
###### H6 - Smallest
```

---

## Text Formatting

| Style | Syntax | Result |
|-------|--------|--------|
| Bold | `**bold**` or `__bold__` | **bold** |
| Italic | `*italic*` or `_italic_` | *italic* |
| Bold + Italic | `***both***` | ***both*** |
| Strikethrough | `~~crossed out~~` | ~~crossed out~~ |
| Highlight | `==highlighted==` | ==highlighted== |
| Inline code | `` `code` `` | `code` |

---

## Lists

### Unordered
```markdown
- Item one
- Item two
  - Nested item
  - Another nested
- Item three
```

- Item one
- Item two
  - Nested item
  - Another nested
- Item three

### Ordered
```markdown
1. First
2. Second
3. Third
   1. Sub-item
   2. Another sub-item
```

1. First
2. Second
3. Third
   1. Sub-item
   2. Another sub-item

### Task Lists
```markdown
- [ ] Unchecked task
- [x] Completed task
- [ ] Another todo
```

- [ ] Unchecked task
- [x] Completed task
- [ ] Another todo

---

## Links & Images

```markdown
[Link text](https://example.com)
[Link with title](https://example.com "Hover title")

![Alt text](image.jpg)
![Alt text](image.jpg "Image title")

[[Internal Link]] (Obsidian wiki-link)
[[Internal Link|Display Text]]
```

---

## Blockquotes

```markdown
> Single line quote

> Multi-line quote
> continues here
>
> > Nested quote
```

> Single line quote

> Multi-line quote
> continues here
>
> > Nested quote

---

## Code Blocks

````markdown
```python
def hello():
    print("Hello, world!")
```

```javascript
console.log("Hello!");
```

```bash
echo "Hello from the shell"
```
````

```python
def hello():
    print("Hello, world!")
```

---

## Tables

```markdown
| Left | Center | Right |
|:-----|:------:|------:|
| L1   | C1     | R1    |
| L2   | C2     | R2    |
```

| Left | Center | Right |
|:-----|:------:|------:|
| L1   | C1     | R1    |
| L2   | C2     | R2    |

---

## Horizontal Rules

```markdown
---
***
___
```

---

## Footnotes

```markdown
Here's a sentence with a footnote.[^1]

[^1]: This is the footnote content.
```

Here's a sentence with a footnote.[^1]

[^1]: This is the footnote content.

---

## Escape Characters

Use backslash to escape special characters:

```markdown
\* Not italic \*
\# Not a header
\[Not a link\]
```

\* Not italic \*

---

## Obsidian-Specific

### Callouts
```markdown
> [!note]
> This is a note callout

> [!warning]
> This is a warning

> [!tip]
> Pro tip here

> [!info]
> Information callout
```

> [!note]
> This is a note callout

> [!warning]
> This is a warning

### Tags
```markdown
#tag #nested/tag
```

### Embeds
```markdown
![[Other Note]]
![[Note#Heading]]
![[image.png|300]]  (width 300px)
```

### Math (LaTeX)
```markdown
Inline: $E = mc^2$

Block:
$$
\frac{n!}{k!(n-k)!} = \binom{n}{k}
$$
```

Inline: $E = mc^2$

$$
\frac{n!}{k!(n-k)!} = \binom{n}{k}
$$

---

## Quick Reference Table

| What | How |
|------|-----|
| Bold | `**text**` |
| Italic | `*text*` |
| Code | `` `code` `` |
| Link | `[text](url)` |
| Image | `![alt](url)` |
| Header | `# to ######` |
| Quote | `> text` |
| List | `- item` or `1. item` |
| Task | `- [ ] task` |
| Table | `\| col \| col \|` |
| Rule | `---` |
| Escape | `\character` |

---

*Created by Jules • 2026-02-07*