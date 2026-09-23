.PHONY: genbeans genddl

default: genbeans genddl

genbeans:
	./bin/genbeans.exe -schema ./pdb.xml

genddl:
	./bin/genddl.exe -schema ./pdb.xml