name: Stitch Issue Agent

on:
  issues:
    types: [opened, edited]
  issue_comment:
    types: [created]

permissions:
  contents: write
  issues: write

jobs:
  generate:
    if: contains(github.event.issue.body, '@stitch') || contains(github.event.comment.body, '@stitch')
    runs-on: ubuntu-latest

    steps:
      - name: Checkout repository
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.22'

      - name: Generate HTML from Google Stitch
        env:
          STITCH_AI_KEY: ${{ secrets.STITCH_AI_KEY }}
          PROMPT: ${{ github.event.comment.body || github.event.issue.body }}
          ISSUE_NUMBER: ${{ github.event.issue.number }}
        run: go run main.go

      - name: Commit generated HTML
        run: |
          git config user.name "github-actions[bot]"
          git config user.email "github-actions[bot]@://github.com"
          git add stitch-generated-*.html
          git commit -m "Add Stitch generated page for issue #${{ github.event.issue.number }}"
          git push
