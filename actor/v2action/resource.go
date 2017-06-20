package v2action

import (
	"archive/zip"
	"crypto/sha1"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"code.cloudfoundry.org/cli/api/cloudcontroller/ccv2"
	"code.cloudfoundry.org/ykk"
	log "github.com/sirupsen/logrus"
)

const (
	DefaultFolderPermissions      = 0755
	DefaultArchiveFilePermissions = 0744
)

type FileChangedError struct {
	Filename string
}

func (e FileChangedError) Error() string {
	return fmt.Sprint("SHA1 mismatch for:", e.Filename)
}

type Resource ccv2.Resource

// GatherArchiveResources returns a list of resources for a directory.
func (actor Actor) GatherArchiveResources(archivePath string) ([]Resource, error) {
	var resources []Resource

	archive, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer archive.Close()

	reader, err := actor.newArchiveReader(archive)
	if err != nil {
		return nil, err
	}

	for _, archivedFile := range reader.File {
		resource := Resource{Filename: filepath.ToSlash(archivedFile.Name)}
		if archivedFile.FileInfo().IsDir() {
			resource.Mode = DefaultFolderPermissions
		} else {
			fileReader, err := archivedFile.Open()
			if err != nil {
				return nil, err
			}
			defer fileReader.Close()

			hash := sha1.New()

			_, err = io.Copy(hash, fileReader)
			if err != nil {
				return nil, err
			}

			resource.Mode = DefaultArchiveFilePermissions
			resource.SHA1 = fmt.Sprintf("%x", hash.Sum(nil))
			resource.Size = archivedFile.FileInfo().Size()
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

// GatherDirectoryResources returns a list of resources for a directory.
func (_ Actor) GatherDirectoryResources(sourceDir string) ([]Resource, error) {
	var resources []Resource
	walkErr := filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		resource := Resource{
			Filename: filepath.ToSlash(relPath),
		}

		if info.IsDir() {
			resource.Mode = DefaultFolderPermissions
		} else {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			sum := sha1.New()
			_, err = io.Copy(sum, file)
			if err != nil {
				return err
			}

			resource.Mode = fixMode(info.Mode())
			resource.SHA1 = fmt.Sprintf("%x", sum.Sum(nil))
			resource.Size = info.Size()
		}
		resources = append(resources, resource)
		return nil
	})

	return resources, walkErr
}

// ZipArchiveResources zips an archive and a sorted (based on full
// path/filename) list of resources and returns the location. On Windows, the
// filemode for user is forced to be readable and executable.
func (actor Actor) ZipArchiveResources(sourceArchivePath string, filesToInclude []Resource) (string, error) {
	log.WithField("sourceArchive", sourceArchivePath).Info("zipping source files from archive")
	zipFile, err := ioutil.TempFile("", "cf-cli-")
	if err != nil {
		return "", err
	}
	defer zipFile.Close()

	writer := zip.NewWriter(zipFile)
	defer writer.Close()

	source, err := os.Open(sourceArchivePath)
	if err != nil {
		return "", err
	}
	defer source.Close()

	reader, err := actor.newArchiveReader(source)
	if err != nil {
		return "", err
	}

	for _, archiveFile := range reader.File {
		log.WithField("archiveFileName", archiveFile.Name).Debug("zipping file")

		resource := actor.findInResources(archiveFile.Name, filesToInclude)
		reader, openErr := archiveFile.Open()
		if openErr != nil {
			log.WithField("archiveFile", archiveFile.Name).Errorln("opening path in dir:", openErr)
			return "", openErr
		}

		err = actor.addFileToZipFromFileSystem(
			archiveFile.Name, reader, archiveFile.FileInfo(),
			archiveFile.Name, resource.SHA1, resource.Mode, writer,
		)
		if err != nil {
			log.WithField("archiveFileName", archiveFile.Name).Errorln("zipping file:", err)
			return "", err
		}
	}

	log.WithFields(log.Fields{
		"zip_file_location": zipFile.Name(),
		"zipped_file_count": len(filesToInclude),
	}).Info("zip file created")
	return zipFile.Name(), nil
}

// ZipDirectoryResources zips a directory and a sorted (based on full
// path/filename) list of resources and returns the location. On Windows, the
// filemode for user is forced to be readable and executable.
func (actor Actor) ZipDirectoryResources(sourceDir string, filesToInclude []Resource) (string, error) {
	log.WithField("sourceDir", sourceDir).Info("zipping source files from directory")
	zipFile, err := ioutil.TempFile("", "cf-cli-")
	if err != nil {
		return "", err
	}
	defer zipFile.Close()

	writer := zip.NewWriter(zipFile)
	defer writer.Close()

	for _, resource := range filesToInclude {
		fullPath := filepath.Join(sourceDir, resource.Filename)
		log.WithField("fullPath", fullPath).Debug("zipping file")

		srcFile, err := os.Open(fullPath)
		if err != nil {
			log.WithField("fullPath", fullPath).Errorln("opening path in dir:", err)
			return "", err
		}

		fileInfo, err := srcFile.Stat()
		if err != nil {
			log.WithField("fullPath", fullPath).Errorln("stat error in dir:", err)
			return "", err
		}

		err = actor.addFileToZipFromFileSystem(
			fullPath, srcFile, fileInfo,
			resource.Filename, resource.SHA1, resource.Mode, writer,
		)
		if err != nil {
			log.WithField("fullPath", fullPath).Errorln("zipping file:", err)
			return "", err
		}
	}

	log.WithFields(log.Fields{
		"zip_file_location": zipFile.Name(),
		"zipped_file_count": len(filesToInclude),
	}).Info("zip file created")
	return zipFile.Name(), nil
}

func (_ Actor) actorToCCResources(resources []Resource) []ccv2.Resource {
	apiResources := make([]ccv2.Resource, 0, len(resources)) // Explicitly done to prevent nils

	for _, resource := range resources {
		apiResources = append(apiResources, ccv2.Resource(resource))
	}

	return apiResources
}

func (_ Actor) addFileToZipFromFileSystem(
	srcPath string, srcFile io.ReadCloser, fileInfo os.FileInfo,
	destPath string, sha1Sum string, mode os.FileMode, zipFile *zip.Writer,
) error {
	defer srcFile.Close()

	header, err := zip.FileInfoHeader(fileInfo)
	if err != nil {
		log.WithField("srcPath", srcPath).Errorln("getting file info in dir:", err)
		return err
	}

	// An extra '/' indicates that this file is a directory
	if fileInfo.IsDir() && !strings.HasSuffix(destPath, "/") {
		destPath += "/"
	}

	header.Name = destPath
	header.Method = zip.Deflate

	header.SetMode(mode)
	log.WithFields(log.Fields{
		"srcPath":  srcPath,
		"destPath": destPath,
		"mode":     mode,
	}).Debug("setting mode for file")

	destFileWriter, err := zipFile.CreateHeader(header)
	if err != nil {
		log.Errorln("creating header:", err)
		return err
	}

	if !fileInfo.IsDir() {
		sum := sha1.New()

		multi := io.MultiWriter(sum, destFileWriter)
		if _, err := io.Copy(multi, srcFile); err != nil {
			log.WithField("srcPath", srcPath).Errorln("copying data in dir:", err)
			return err
		}

		if currentSum := fmt.Sprintf("%x", sum.Sum(nil)); sha1Sum != currentSum {
			log.WithFields(log.Fields{
				"expected":   sha1Sum,
				"currentSum": currentSum,
			}).Error("setting mode for file")
			return FileChangedError{Filename: srcPath}
		}
	}

	return nil
}

func (_ Actor) findInResources(path string, filesToInclude []Resource) Resource {
	for _, resource := range filesToInclude {
		if resource.Filename == path {
			log.WithField("resource", resource.Filename).Debug("found resource in files to include")
			return resource
		}
	}

	log.WithField("path", path).Debug("did not find resource in files to include")
	return Resource{}
}

func (_ Actor) newArchiveReader(archive *os.File) (*zip.Reader, error) {
	info, err := archive.Stat()
	if err != nil {
		return nil, err
	}

	return ykk.NewReader(archive, info.Size())
}
