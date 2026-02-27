/**
 * Conduit Go - Promotional Website Scripts
 * Feature carousel, copy-to-clipboard, smooth scroll
 */

document.addEventListener('DOMContentLoaded', () => {
    initCarousel();
    initCopyButtons();
    initSmoothScroll();
});

/**
 * Feature Carousel Tab Switching
 */
function initCarousel() {
    const tabs = document.querySelectorAll('.carousel-tab');
    const panels = document.querySelectorAll('.carousel-panel');

    tabs.forEach(tab => {
        tab.addEventListener('click', () => {
            const targetPanel = tab.dataset.tab;

            // Update tabs
            tabs.forEach(t => t.classList.remove('active'));
            tab.classList.add('active');

            // Update panels
            panels.forEach(panel => {
                panel.classList.remove('active');
                if (panel.dataset.panel === targetPanel) {
                    panel.classList.add('active');
                }
            });
        });
    });
}

/**
 * Copy-to-Clipboard for Code Blocks
 */
function initCopyButtons() {
    const copyButtons = document.querySelectorAll('.copy-btn');

    copyButtons.forEach(btn => {
        btn.addEventListener('click', async () => {
            const targetId = btn.dataset.copy;
            const codeBlock = document.getElementById(targetId);

            if (!codeBlock) return;

            // Get text content, stripping the $ prompts
            const text = codeBlock.textContent
                .split('\n')
                .map(line => line.replace(/^\$\s*/, ''))
                .filter(line => !line.startsWith('#') && line.trim())
                .join('\n');

            try {
                await navigator.clipboard.writeText(text);

                // Visual feedback
                const originalText = btn.textContent;
                btn.textContent = 'Copied!';
                btn.classList.add('copied');

                setTimeout(() => {
                    btn.textContent = originalText;
                    btn.classList.remove('copied');
                }, 2000);
            } catch (err) {
                console.error('Failed to copy:', err);
                btn.textContent = 'Failed';
                setTimeout(() => {
                    btn.textContent = 'Copy';
                }, 2000);
            }
        });
    });
}

/**
 * Smooth Scroll for Navigation Links
 */
function initSmoothScroll() {
    const navLinks = document.querySelectorAll('a[href^="#"]');

    navLinks.forEach(link => {
        link.addEventListener('click', (e) => {
            const targetId = link.getAttribute('href');
            if (targetId === '#') return;

            const targetElement = document.querySelector(targetId);
            if (!targetElement) return;

            e.preventDefault();

            const headerHeight = document.querySelector('.header').offsetHeight;
            const targetPosition = targetElement.offsetTop - headerHeight - 20;

            window.scrollTo({
                top: targetPosition,
                behavior: 'smooth'
            });
        });
    });
}
