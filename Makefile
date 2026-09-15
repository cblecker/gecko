.PHONY: lint lint-fix lint-fmt
lint lint-fix lint-fmt:
	@rc=0; for module in orlop platform-api controllers; do \
		$(MAKE) -C $$module $@ || rc=1; \
	done; exit $$rc

.PHONY: test
test:
	$(MAKE) -C orlop test
	$(MAKE) -C platform-api test
	$(MAKE) -C controllers test
