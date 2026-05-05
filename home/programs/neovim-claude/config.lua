require("claudecode").setup({
  terminal = {
    split_side = "right",
    split_width_percentage = 0.4,
  },
})

vim.keymap.set("n", "<leader>cc", "<cmd>ClaudeCodeToggle<CR>", { desc = "Toggle Claude Code" })
vim.keymap.set("n", "<leader>cf", "<cmd>ClaudeCodeFocus<CR>", { desc = "Focus Claude" })
vim.keymap.set("n", "<leader>cr", "<cmd>ClaudeCodeResume<CR>", { desc = "Resume Claude session" })
vim.keymap.set("n", "<leader>ca", "<cmd>ClaudeCodeAdd<CR>", { desc = "Add buffer to Claude" })
vim.keymap.set("v", "<leader>cs", "<cmd>ClaudeCodeSend<CR>", { desc = "Send selection to Claude" })
