# Extension Mode Example

This example shows how to add a custom agent mode — no packaging needed.

## Structure

```
extension-mode/
  review.yaml     # the mode definition
```

## Usage

Copy the mode file into one of the mode search directories:

```bash
# user level
cp review.yaml ~/.lcoder/modes/

# or project level
cp review.yaml <your-project>/.lcoder/modes/
```

Then run:

```bash
lcoder modes
lcoder --mode review "review pkg/agent/loop.go"
```

Project-level modes override user-level modes with the same name; both
override the embedded defaults.
