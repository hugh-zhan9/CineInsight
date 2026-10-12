import Foundation
import NaturalLanguage
while let line=readLine(){
 do{
  let request=try JSONSerialization.jsonObject(with:Data(line.utf8)) as! [String:Any]
  let language=NLLanguage(rawValue:request["language"] as? String ?? "en")
  guard let embedding=NLEmbedding.sentenceEmbedding(for:language) else { print("{\"error\":\"language_unavailable\"}");continue }
  let texts=request["texts"] as! [String]
  let vectors=texts.map { embedding.vector(for:$0) ?? [] }
  let result:[String:Any]=["dimension":embedding.dimension,"vectors":vectors]
  let data=try JSONSerialization.data(withJSONObject:result);print(String(data:data,encoding:.utf8)!);fflush(stdout)
 }catch{print("{\"error\":\"embedding_failed\"}")}
}
